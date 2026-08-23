output "api_url" {
  description = "Where the REST API and the dashboard's origin answer."
  value       = google_cloud_run_v2_service.control_plane["api"].uri
}

output "proxy_url" {
  description = "Where deployed environments are reached. Point the wildcard record for base_domain at this."
  value       = google_cloud_run_v2_service.control_plane["proxy"].uri
}

output "control_plane_service_account" {
  description = "The control plane's identity, as an IAM member string."
  value       = google_service_account.control_plane.member
}

output "workload_service_account" {
  description = "The identity deployed applications run as, as an IAM member string."
  value       = google_service_account.workload.member
}

output "workload_service_account_email" {
  description = "LAUNCHPAD_CLOUD_RUN_SERVICE_ACCOUNT, for a control plane configured by hand."
  value       = google_service_account.workload.email
}

output "build_service_account" {
  description = "Cloud Build's default identity, as an IAM member string. It has to be allowed to push to the image repository."
  value       = "serviceAccount:${local.build_service_account}"
}
