output "repository_id" {
  description = "Repository ID, which is what LAUNCHPAD_ARTIFACT_REPOSITORY is set to."
  value       = google_artifact_registry_repository.images.repository_id
}

output "registry_host" {
  description = "Docker registry hostname for this region."
  value       = "${var.region}-docker.pkg.dev"
}

output "repository_url" {
  description = "Prefix the Cloud Run driver qualifies image tags with."
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.images.repository_id}"
}
