output "instance_name" {
  description = "Cloud SQL instance name."
  value       = google_sql_database_instance.main.name
}

output "connection_name" {
  description = "project:region:instance, which is how Cloud Run attaches the instance."
  value       = google_sql_database_instance.main.connection_name
}

output "database_url_secret_id" {
  description = "Secret Manager secret holding the connection URL."
  value       = google_secret_manager_secret.database_url.secret_id
}

output "database_url" {
  description = "Connection URL. Marked sensitive so it is not printed by a plan or an apply; it is still in state, which is why state belongs in a bucket nobody has casual access to."
  value       = local.database_url
  sensitive   = true
}
