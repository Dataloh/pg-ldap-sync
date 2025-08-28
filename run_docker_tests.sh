#!/bin/bash

# Exit immediately if a command exits with a non-zero status.
set -e
# Treat unset variables as an error when substituting.
set -u

# --- Helper Functions for Logging and Verification ---
log_step() {
  echo -e "\n\033[1;34m=== $1 ===\033[0m"
}

log_success() {
  echo -e "\033[1;32m✅ $1\033[0m"
}

run_sync_job() {
  log_step "Running Go sync job..."
  export PG_PASSWORD="supersecretpassword"
  export LDAP_BIND_DN="cn=admin,dc=example,dc=org"
  export LDAP_BIND_PASSWORD="adminpassword"

  docker run \
      -e PG_PASSWORD \
      -e LDAP_BIND_DN \
      -e LDAP_BIND_PASSWORD \
      --network pg-ldap-sync_dev-net \
      --rm \
      pg-ldap-sync:latest
  }

# ... (All verify_* helper functions are unchanged) ...
verify_membership() {
  local group_role=$1
  local user_role=$2
  echo "VERIFYING: User '$user_role' should be in group '$group_role'"
  sudo docker exec postgres psql -U pgadmin -d myapp_db -t -c \
    "SELECT 1 FROM pg_catalog.pg_roles u JOIN pg_catalog.pg_auth_members m ON m.member = u.oid JOIN pg_catalog.pg_roles g ON m.roleid = g.oid WHERE g.rolname = '$group_role' AND u.rolname = '$user_role'" \
    | grep -q 1
}
verify_no_membership() {
  local group_role=$1
  local user_role=$2
  echo "VERIFYING: User '$user_role' should NOT be in group '$group_role'"
  if ! sudo docker exec postgres psql -U pgadmin -d myapp_db -t -c \
    "SELECT 1 FROM pg_catalog.pg_roles u JOIN pg_catalog.pg_auth_members m ON m.member = u.oid JOIN pg_catalog.pg_roles g ON m.roleid = g.oid WHERE g.rolname = '$group_role' AND u.rolname = '$user_role'" \
    | grep -q 1; then return 0; else return 1; fi
}
verify_user_does_not_exist() {
  local user_role=$1
  echo "VERIFYING: User role '$user_role' should NOT exist"
  if ! sudo docker exec postgres psql -U pgadmin -d myapp_db -t -c \
    "SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = '$user_role'" \
    | grep -q 1; then return 0; else return 1; fi
}

# --- Main Test Execution ---

# Cleanup function to run on exit
cleanup() {
  log_step "Cleaning up..."
  sudo docker compose down -v
  rm -f *.ldif.tmp
  log_success "Test environment cleaned up."
  rm config.yml
}
trap cleanup EXIT

log_step "SETUP: Creating symlink for config.file"
ln -s config.yml.docker config.yml

log_step "SETUP: Starting Docker environment"
sudo docker compose down -v
sudo docker compose up -d --build
sleep 3

log_step "SETUP: Building Docker image"
docker build . -t pg-ldap-sync

#log_success "SETUP: Complete"
log_step "DATABASE STATE BEFORE TESTS"
sudo docker exec -it postgres psql -U pgadmin -d myapp_db -c "\du"

# --- TEST CASE 1: INITIAL SYNC ---
log_step "TEST CASE 1: Initial Sync"
run_sync_job
verify_membership "ldap_db_admins" "nc_jdoe"
verify_membership "ldap_db_admins" "admin_nc_asmith"
verify_membership "ldap_readonly_users" "nc_bcarter"
verify_membership "g_ldapusers" "nc_jdoe"
log_step "DATABASE STATE AFTER TEST CASE 1"
sudo docker exec -it postgres psql -U pgadmin -d myapp_db -c "\du"
log_success "TEST CASE 1 PASSED"

# --- TEST CASE 2: USER DEPROVISIONING ---
log_step "TEST CASE 2: Deprovisioning (by deleting the readonly_users group)"
sudo docker exec openldap ldapdelete -x -H ldap://localhost:1389 -D "cn=admin,dc=example,dc=org" -w "adminpassword" "cn=readonly_users,ou=groups,dc=example,dc=org"
run_sync_job
verify_user_does_not_exist "nc_bcarter"
log_step "DATABASE STATE AFTER TEST CASE 2"
sudo docker exec -it postgres psql -U pgadmin -d myapp_db -c "\du"
log_success "TEST CASE 2 PASSED"

# --- TEST CASE 3: NEW USER IN MULTIPLE GROUPS ---
log_step "TEST CASE 3: Add new user 'nc_testuser' to two groups"
# Create an LDIF for the new user
cat <<EOF > add_testuser.ldif.tmp
dn: cn=nc_testuser,ou=users,dc=example,dc=org
objectClass: inetOrgPerson
cn: nc_testuser
sn: Test
uid: nc_testuser
userPassword: password123
EOF
# Create an LDIF to re-create readonly_users and to modify db_admins
cat <<EOF > modify_groups.ldif.tmp
# Re-create readonly_users with nc_testuser as its first member
dn: cn=readonly_users,ou=groups,dc=example,dc=org
objectClass: groupOfNames
cn: readonly_users
member: cn=nc_testuser,ou=users,dc=example,dc=org

# Add nc_testuser to the existing db_admins group
dn: cn=db_admins,ou=groups,dc=example,dc=org
changetype: modify
add: member
member: cn=nc_testuser,ou=users,dc=example,dc=org
EOF
# Copy and execute the changes
sudo docker cp add_testuser.ldif.tmp openldap:/tmp/add_testuser.ldif
sudo docker cp modify_groups.ldif.tmp openldap:/tmp/modify_groups.ldif
sudo docker exec openldap ldapadd -x -H ldap://localhost:1389 -D "cn=admin,dc=example,dc=org" -w "adminpassword" -f /tmp/add_testuser.ldif
sudo docker exec openldap ldapmodify -a -x -H ldap://localhost:1389 -D "cn=admin,dc=example,dc=org" -w "adminpassword" -f /tmp/modify_groups.ldif
run_sync_job
verify_membership "ldap_db_admins" "nc_testuser"
verify_membership "ldap_readonly_users" "nc_testuser"
log_step "DATABASE STATE AFTER TEST CASE 3"
sudo docker exec -it postgres psql -U pgadmin -d myapp_db -c "\du"
log_success "TEST CASE 3 PASSED"

# --- TEST CASE 4: PARTIAL MEMBERSHIP REVOKE ---
log_step "TEST CASE 4: Remove 'nc_testuser' from one group"
# Create an LDIF to remove the user from just the db_admins group
cat <<EOF > remove_testuser_from_dbadmins.ldif.tmp
dn: cn=db_admins,ou=groups,dc=example,dc=org
changetype: modify
delete: member
member: cn=nc_testuser,ou=users,dc=example,dc=org
EOF
# Copy and execute the change
sudo docker cp remove_testuser_from_dbadmins.ldif.tmp openldap:/tmp/remove_testuser_from_dbadmins.ldif
sudo docker exec openldap ldapmodify -x -H ldap://localhost:1389 -D "cn=admin,dc=example,dc=org" -w "adminpassword" -f /tmp/remove_testuser_from_dbadmins.ldif
run_sync_job
verify_membership "ldap_readonly_users" "nc_testuser"
verify_membership "g_ldapusers" "nc_testuser"
verify_no_membership "ldap_db_admins" "nc_testuser"
log_step "DATABASE STATE AFTER TEST CASE 4"
sudo docker exec -it postgres psql -U pgadmin -d myapp_db -c "\du"
log_success "TEST CASE 4 PASSED"

echo -e "\n\033[1;32m🎉 ALL TESTS PASSED SUCCESSFULLY 🎉\033[0m"

