# The control plane, as two Cloud Run services from one image.
#
# The binary serves two listeners: the REST API on LAUNCHPAD_HTTP_ADDR and the
# environment proxy on LAUNCHPAD_PROXY_ADDR. Cloud Run publishes one port per
# service, so the same image runs twice with the ports swapped — the API service
# publishes the API and keeps the proxy on a port nothing routes to, and the
# proxy service does the reverse. Splitting the binary in two would be the other
# answer, and would buy a second deployable to operate for no gain: they share
# the store, the migrations and the configuration.

data "google_project" "this" {
  project_id = var.project_id
}

locals {
  # Cloud Build's default identity, which is what "gcloud builds submit" runs
  # as. Exported so the Artifact Registry module can let it push.
  build_service_account = "${data.google_project.this.number}@cloudbuild.gserviceaccount.com"

  # gcloud builds submit uploads the build context here and does not pass a
  # bucket, so the name is not ours to choose.
  build_staging_bucket = "${var.project_id}_cloudbuild"

  common_env = {
    LAUNCHPAD_ENV                       = "production"
    LAUNCHPAD_LOG_LEVEL                 = var.log_level
    LAUNCHPAD_BASE_DOMAIN               = var.base_domain
    LAUNCHPAD_DEPLOY_DRIVER             = "cloudrun"
    LAUNCHPAD_GCP_PROJECT               = var.project_id
    LAUNCHPAD_GCP_REGION                = var.region
    LAUNCHPAD_ARTIFACT_REPOSITORY       = var.artifact_repository
    LAUNCHPAD_CLOUD_RUN_SERVICE_ACCOUNT = google_service_account.workload.email
  }

  services = {
    api = {
      env = merge(local.common_env, {
        LAUNCHPAD_HTTP_ADDR      = ":8080"
        LAUNCHPAD_PROXY_ADDR     = ":8081"
        LAUNCHPAD_DEPLOY_WORKERS = tostring(var.deploy_workers)
      })
      # /healthz deliberately touches no dependencies, so it answers what a
      # probe is actually asking: is this process alive. A database blip must
      # not convince Cloud Run to restart an otherwise healthy instance.
      health_path = "/healthz"
    }

    proxy = {
      env = merge(local.common_env, {
        LAUNCHPAD_HTTP_ADDR  = ":8081"
        LAUNCHPAD_PROXY_ADDR = ":8080"
        # No deploy workers here. The API service runs them; a second set would
        # only compete for the same queue, and this service is scaled by the
        # traffic of deployed applications rather than by deployments.
        LAUNCHPAD_DEPLOY_WORKERS = "0"
      })
      # The proxy routes by hostname and answers 404 for a hostname it has no
      # environment for, so it has no path of its own to probe. A TCP check is
      # the honest test of whether it is listening.
      health_path = null
    }
  }
}

# The control plane's own identity. It deploys services, submits builds and
# reads its database password; it is not the identity anything it deploys runs
# as.
resource "google_service_account" "control_plane" {
  project      = var.project_id
  account_id   = "${var.name}-control-plane"
  display_name = "Launchpad control plane"
}

# The identity deployed workloads run as. Separate from the one above, and given
# nothing at all: it exists so that other people's code does not run as the
# project's default compute account, which can read every bucket in the project.
resource "google_service_account" "workload" {
  project      = var.project_id
  account_id   = "${var.name}-workload"
  display_name = "Launchpad deployed workloads"
  description  = "Runtime identity for applications Launchpad deploys. Deliberately holds no project permissions."
}

resource "google_project_iam_member" "control_plane" {
  for_each = toset([
    # Deploy Cloud Run services. Not run.admin: creating and updating services
    # does not require being able to rewrite their IAM policies.
    "roles/run.developer",
    # Submit builds and read their status.
    "roles/cloudbuild.builds.editor",
    # Attach the Cloud SQL instance.
    "roles/cloudsql.client",
    "roles/logging.logWriter",
  ])

  project = var.project_id
  role    = each.value
  member  = google_service_account.control_plane.member
}

# Deploying a service means telling Cloud Run which identity to run it as, and
# Google treats that as impersonation. Granted on the workload account alone: at
# the project level this role would let the control plane act as every service
# account in the project, including its own.
resource "google_service_account_iam_member" "control_plane_uses_workload" {
  service_account_id = google_service_account.workload.name
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.control_plane.member
}

# gcloud builds submit uploads the build context to this bucket. Created here so
# the permission to write to it can be scoped to the bucket, instead of granting
# the control plane storage.admin across the project as the quickstart does.
resource "google_storage_bucket" "build_staging" {
  project                     = var.project_id
  name                        = local.build_staging_bucket
  location                    = var.region
  uniform_bucket_level_access = true
  force_destroy               = true

  # Somebody else's source code, uploaded so it can be built. It has no reason
  # to still be here tomorrow.
  lifecycle_rule {
    condition {
      age = 1
    }
    action {
      type = "Delete"
    }
  }
}

resource "google_storage_bucket_iam_member" "control_plane_stages_builds" {
  bucket = google_storage_bucket.build_staging.name
  role   = "roles/storage.objectAdmin"
  member = google_service_account.control_plane.member
}

resource "google_secret_manager_secret_iam_member" "database_url" {
  project   = var.project_id
  secret_id = var.database_url_secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.control_plane.member
}

resource "google_cloud_run_v2_service" "control_plane" {
  for_each = local.services

  project             = var.project_id
  name                = "${var.name}-${each.key}"
  location            = var.region
  ingress             = "INGRESS_TRAFFIC_ALL"
  deletion_protection = var.deletion_protection

  template {
    service_account = google_service_account.control_plane.email

    scaling {
      # One instance always running, per service. Cloud Run throttles CPU
      # between requests unless told otherwise, and both services do work with
      # no request in flight: the API polls the deployment queue, and both flush
      # the minute of metrics they have accumulated. Scaling to zero would stop
      # a queued deployment from ever being claimed and leave holes in the
      # measurements. This is the standing cost of running the workers in the
      # same process as the API, and it is the honest price of not operating a
      # second deployable.
      min_instance_count = 1
      max_instance_count = var.max_instances
    }

    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [var.database_connection_name]
      }
    }

    containers {
      image = var.image

      ports {
        container_port = 8080
      }

      dynamic "env" {
        for_each = each.value.env
        content {
          name  = env.key
          value = env.value
        }
      }

      # Read from Secret Manager at instance start rather than baked into the
      # service. A connection string set as a plain value is legible to anyone
      # who can describe the service, which is a wider group than the people who
      # should have the database password.
      env {
        name = "LAUNCHPAD_DATABASE_URL"
        value_source {
          secret_key_ref {
            secret  = var.database_url_secret_id
            version = "latest"
          }
        }
      }

      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }

      resources {
        limits = {
          cpu    = var.cpu
          memory = var.memory
        }
        # See min_instance_count: background work between requests needs the CPU
        # to still be there between requests.
        cpu_idle          = false
        startup_cpu_boost = true
      }

      dynamic "startup_probe" {
        for_each = each.value.health_path == null ? [] : [each.value.health_path]
        content {
          initial_delay_seconds = 5
          timeout_seconds       = 3
          period_seconds        = 5
          failure_threshold     = 6

          http_get {
            path = startup_probe.value
          }
        }
      }

      dynamic "startup_probe" {
        for_each = each.value.health_path == null ? [1] : []
        content {
          initial_delay_seconds = 5
          timeout_seconds       = 3
          period_seconds        = 5
          failure_threshold     = 6

          tcp_socket {
            port = 8080
          }
        }
      }

      dynamic "liveness_probe" {
        for_each = each.value.health_path == null ? [] : [each.value.health_path]
        content {
          period_seconds    = 30
          timeout_seconds   = 5
          failure_threshold = 3

          http_get {
            path = liveness_probe.value
          }
        }
      }
    }
  }

  depends_on = [
    google_secret_manager_secret_iam_member.database_url,
    google_storage_bucket_iam_member.control_plane_stages_builds,
  ]
}

# Both services are reached by the public. The API is not unauthenticated — it
# requires a bearer token on every route but two — and the proxy has to be
# reachable by whoever is looking at a deployed application. Cloud Run's IAM
# check is the wrong layer to express either: an install that wants a network
# boundary in front puts a load balancer there and sets ingress accordingly.
resource "google_cloud_run_v2_service_iam_member" "public" {
  for_each = google_cloud_run_v2_service.control_plane

  project  = each.value.project
  location = each.value.location
  name     = each.value.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}
