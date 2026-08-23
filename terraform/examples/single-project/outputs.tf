output "api_url" {
  description = "Point the dashboard at this, and mint the first token with `launchpad-bootstrap` against the database."
  value       = module.control_plane.api_url
}

output "proxy_url" {
  description = "Deployed environments answer here. The wildcard record for base_domain has to reach it."
  value       = module.control_plane.proxy_url
}

output "image_repository" {
  description = "Push the control-plane image here."
  value       = module.images.repository_url
}

output "database_connection_name" {
  description = "Pass this to the Cloud SQL Auth Proxy to run migrations or `launchpad-bootstrap` from a laptop."
  value       = module.database.connection_name
}
