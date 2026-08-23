# The platform's own state: accounts, projects, services, environments,
# deployments, build logs and metric buckets.
#
# The control plane migrates this schema on boot, so nothing here creates
# tables. What it creates is the instance, the database, a user, and a Secret
# Manager secret holding the connection URL — because a connection string
# belongs in a secret the runtime reads at start, not in an environment variable
# that shows up in a service description.

resource "google_sql_database_instance" "main" {
  project             = var.project_id
  name                = var.name
  region              = var.region
  database_version    = var.database_version
  deletion_protection = var.deletion_protection

  settings {
    tier              = var.tier
    availability_type = var.availability_type
    disk_size         = var.disk_size_gb
    disk_autoresize   = true

    backup_configuration {
      enabled = true
      # Point-in-time recovery is what turns "we have last night's backup" into
      # "we can go back to the minute before the mistake".
      point_in_time_recovery_enabled = true
      start_time                     = "03:00"
      transaction_log_retention_days = 7

      backup_retention_settings {
        retained_backups = var.backup_retention_days
        retention_unit   = "COUNT"
      }
    }

    ip_configuration {
      # A public IP with no authorized networks. Cloud Run attaches the instance
      # through the Cloud SQL connector, which authorises by IAM and connects
      # over a socket rather than an address, so nothing reaches this IP without
      # a credential — and the alternative, a private-IP instance, needs a VPC
      # and a service networking peering that an install this size does not.
      ipv4_enabled = true
      ssl_mode     = "ENCRYPTED_ONLY"
    }

    maintenance_window {
      day          = 7
      hour         = 4
      update_track = "stable"
    }

    database_flags {
      # Enough to see which statement was running when something locked up,
      # without logging every SELECT the dashboard makes.
      name  = "log_min_duration_statement"
      value = "1000"
    }
  }
}

resource "google_sql_database" "launchpad" {
  project  = var.project_id
  instance = google_sql_database_instance.main.name
  name     = "launchpad"
}

# Generated rather than passed in: a password that a human chose is a password
# that exists somewhere a human put it.
resource "random_password" "app" {
  length = 32
  # Excludes characters that would have to be percent-encoded in the URL the
  # secret holds, which is a class of outage nobody enjoys diagnosing.
  override_special = "-_~."
  special          = true
}

resource "google_sql_user" "app" {
  project  = var.project_id
  instance = google_sql_database_instance.main.name
  name     = "launchpad"
  password = random_password.app.result
}

locals {
  # The Cloud SQL connector presents the instance as a unix socket under
  # /cloudsql, which is why the URL names a directory instead of a host. TLS is
  # off because the hop is a socket inside the container's own sandbox; the
  # connector has already encrypted everything that leaves it.
  database_url = format(
    "postgres://%s:%s@/%s?host=/cloudsql/%s&sslmode=disable",
    google_sql_user.app.name,
    random_password.app.result,
    google_sql_database.launchpad.name,
    google_sql_database_instance.main.connection_name,
  )
}

resource "google_secret_manager_secret" "database_url" {
  project   = var.project_id
  secret_id = "${var.name}-database-url"

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "database_url" {
  secret      = google_secret_manager_secret.database_url.id
  secret_data = local.database_url
}

resource "google_secret_manager_secret_iam_member" "accessors" {
  for_each = toset(var.secret_accessors)

  project   = var.project_id
  secret_id = google_secret_manager_secret.database_url.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = each.value
}
