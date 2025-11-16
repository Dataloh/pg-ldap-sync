# LDAP to PostgreSQL User Sync

This project provides a robust, containerized Go application that synchronizes user memberships from LDAP groups to PostgreSQL roles. It is designed to be configurable, secure, and flexible, supporting both containerized and traditional bare-metal deployments.

The application runs as a periodic job, ensuring that PostgreSQL role memberships are kept in sync with the source of truth in the LDAP directory, including handling complex nested group structures.

## Features

-   **Automated Sync:** Synchronizes user memberships from specified LDAP groups to corresponding PostgreSQL roles.
-   **Recursive Group Membership:** Correctly resolves users from nested LDAP groups (groups within groups) to any depth.
-   **Safe, Prefix-Aware Sync:** All provisioning, deprovisioning, and membership changes are strictly scoped to users matching configured prefixes (e.g., `nc_`). This prevents the accidental removal of unmanaged service accounts or other database groups.
-   **Configurable Mappings:** Uses a simple YAML file to define which LDAP groups map to which PostgreSQL roles, supporting multiple databases.
-   **User Provisioning & Deprovisioning:**
    -   Automatically creates user roles in PostgreSQL if they exist in LDAP but not in the database.
    -   Automatically removes managed user roles from PostgreSQL when they are no longer in any relevant LDAP groups.
-   **Default Group Assignment:** Automatically assigns all synchronized users to a default PostgreSQL group (e.g., `g_ldapuser`).
-   **Dual Testing Modes:** Includes comprehensive end-to-end test scripts for both local binary execution and Docker container-based execution.
-   **Flexible Deployment:** Can be deployed as a `CronJob` in Kubernetes or as a compiled binary scheduled with a traditional system cron.
-   **Secure by Design:** Handles secrets (passwords) via environment variables, separate from the main configuration file.

## Project Structure

```
.
├── cmd/sync/main.go            # Main application entrypoint
├── internal/                   # Internal Go packages (config, ldap, postgres)
├── ldap-init/                  # LDIF files for populating the test LDAP server
├── postgres-init/              # SQL scripts for initializing the test Postgres server
├── config.yml                  # Config for Docker container testing (host: postgres)
├── Dockerfile                  # Dockerfile for the Go sync application
├── docker-compose.yml          # Defines the complete Docker test environment
├── run_tests.sh                # E2E test script that runs the app as a Docker container
└── go.mod                      # Go module definition
```

## Prerequisites

Before you begin, ensure you have the following installed:
-   [Go](https://go.dev/doc/install) (version 1.24 or newer)
-   [Docker](https://docs.docker.com/get-docker/)
-   [Docker Compose](https://docs.docker.com/compose/install/)

## Local Test Environment Setup

Follow these steps to set up the Docker-based environment for local testing.

1.  **Clone the Repository**
    ```sh
    git clone <your-repo-url>
    cd pg-ldap-sync
    ```

2.  **Build the Docker Images**
    This command will build the custom OpenLDAP server image and the Go application image.
    ```sh
    docker compose build
    ```

3.  **Start the Environment**
    This command starts the PostgreSQL and OpenLDAP containers in the background. They will auto-initialize with test data.
    ```sh
    docker compose up -d
    ```
    The services will be ready for testing after a few seconds.

## Configuration

The application is configured using a YAML file and environment variables.

### Environment Variables
Secrets are provided via environment variables. The test scripts load these from a local `.env` file if it exists.

| Variable             | Description                                |
| -------------------- | ------------------------------------------ |
| `PG_PASSWORD`        | The password for the PostgreSQL admin user.|
| `LDAP_BIND_PASSWORD` | The password for the LDAP bind user.       |
| `CFG_PATH`           | The path to config.yml (optional)          |

The path to config.yml defaults to /opt/pg-ldap-sync/config.yml

## Testing the Application

Two comprehensive test scripts are provided to validate the system's functionality.

### Docker Container Test (`run_tests.sh`)
This script builds the application into a Docker image and runs the sync job as a container. This method provides a test that is closer to a production deployment.

1.  **Make the script executable:**
    ```sh
    chmod +x run_tests.sh
    ```
2.  **Execute the script:**
    ```sh
    ./run_tests.sh
    ```
    The script will start a fresh environment, run tests using the `pg-ldap-sync` container and `config.yml.docker`, and report the results.

## Deployment Options

### Option 1: Kubernetes CronJob (Containerized)

This is the recommended approach for containerized environments.

-   **Image:** Push the `pg-ldap-sync` image to a container registry (e.g., Docker Hub, ECR, GCR).
-   **ConfigMap:** Store the contents of your production `config.yml` in a Kubernetes `ConfigMap`. Mount it as a file into the pod at `/app/config.yml`.
-   **Secret:** Store secrets (`PG_PASSWORD`, `LDAP_BIND_PASSWORD`, etc.) in a Kubernetes `Secret` and consume them as environment variables in the pod.
-   **CronJob:** Create a `CronJob` resource that defines the schedule (e.g., `*/15 * * * *`), container image, `ConfigMap`, and `Secret`.

### Option 2: System Cron Job (Binary)

For environments where Kubernetes is not available, the application can be run as a compiled binary scheduled by a system cron job.

1.  **Compile the Binary:** On your target server (or a build machine with the same architecture), build the application.
    ```sh
    CGO_ENABLED=0 GOOS=linux go build -o pg-ldap-sync ./cmd/sync
    ```
2.  **Deploy Files:** Copy the compiled `pg-ldap-sync` binary and your production `config.yml` file to a directory on the server (e.g., `/opt/pg-ldap-sync/`).
3.  **Set Up Environment:** Create a file to store your secrets as environment variables (e.g., `/opt/pg-ldap-sync/secrets.env`).
    ```sh
    # /opt/pg-ldap-sync/secrets.env
    export PG_PASSWORD="supersecretpassword"
    export LDAP_BIND_PASSWORD="bindpassword"
    ```
4.  **Create Cron Job:** Edit the crontab (`crontab -e`) to add an entry that sources the environment variables and runs the binary on a schedule.
    

    ```crontab
        # Run the LDAP sync every 15 minutes
        */15 * * * * /usr/bin/env bash -c 'source /opt/pg-ldap-sync/secrets.env && /opt/pg-ldap-sync/pg-ldap-sync'
    ```
