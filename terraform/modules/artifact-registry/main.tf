# Where built images live.
#
# Launchpad builds an image per deployment and never asks for an old one again
# once its successor is live, so without a cleanup policy this repository grows
# without limit and is billed for it. The policy keeps the most recent versions
# whatever their age — that is the rollback window — and deletes the rest once
# they are old enough that nothing is going back to them.

resource "google_artifact_registry_repository" "images" {
  project       = var.project_id
  location      = var.region
  repository_id = var.name
  format        = "DOCKER"
  description   = "Container images built by Launchpad, tagged by deployment"

  docker_config {
    # A tag names one deployment forever. Making tags immutable turns the
    # remote possibility of two deployment IDs sharing a prefix into a failed
    # push, rather than one deployment quietly serving another's image.
    immutable_tags = true
  }

  # KEEP is evaluated as an exemption from DELETE, so the two policies together
  # read as "delete images older than N days, unless they are among the most
  # recent M".
  cleanup_policies {
    id     = "keep-recent"
    action = "KEEP"

    most_recent_versions {
      keep_count = var.keep_recent_versions
    }
  }

  cleanup_policies {
    id     = "delete-old"
    action = "DELETE"

    condition {
      older_than = "${var.delete_after_days * 24}h"
    }
  }
}

# Scoped to the repository rather than granted on the project. The control plane
# pushes images; that is not a reason for it to be able to read every other
# repository the project may hold.
resource "google_artifact_registry_repository_iam_member" "writers" {
  for_each = toset(var.writers)

  project    = google_artifact_registry_repository.images.project
  location   = google_artifact_registry_repository.images.location
  repository = google_artifact_registry_repository.images.name
  role       = "roles/artifactregistry.writer"
  member     = each.value
}

resource "google_artifact_registry_repository_iam_member" "readers" {
  for_each = toset(var.readers)

  project    = google_artifact_registry_repository.images.project
  location   = google_artifact_registry_repository.images.location
  repository = google_artifact_registry_repository.images.name
  role       = "roles/artifactregistry.reader"
  member     = each.value
}
