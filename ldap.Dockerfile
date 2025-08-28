# Use the official Bitnami image as our base
FROM bitnami/openldap:latest

# Copy all our custom LDIF files into the container
COPY ./ldap-init/data/ /lifs/

