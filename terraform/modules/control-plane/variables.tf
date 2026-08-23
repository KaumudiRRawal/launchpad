variable "project_id" {
  description = "Project the control plane runs in and deploys into."
  type        = string
}

variable "region" {
  description = "Region for the control plane, its builds and everything it deploys."
  type        = string
}

variable "name" {
  description = "Prefix for the two Cloud Run services and the two service accounts."
  type        = string
  default     = "launchpad"
}

variable "image" {
  description = "Control-plane image, built by control-plane/Dockerfile. Name a digest rather than a tag if you want a revision to keep running the bytes it was created with."
  type        = string
}

variable "base_domain" {
  description = "Suffix environment subdomains are minted under. Every deployment's public URL is <environment>-<project>.<base_domain>, so this has to be a domain whose wildcard you can point at the proxy service."
  type        = string
}

variable "artifact_repository" {
  description = "Artifact Registry repository built images are pushed to."
  type        = string
  default     = "launchpad"
}

variable "database_url_secret_id" {
  description = "Secret Manager secret holding the connection URL."
  type        = string
}

variable "database_connection_name" {
  description = "project:region:instance of the Cloud SQL instance to attach."
  type        = string
}

variable "deploy_workers" {
  description = "Concurrent builds per API instance. Builds are submitted to Cloud Build and waited on, so this bounds in-flight deployments rather than local CPU."
  type        = number
  default     = 2
}

variable "log_level" {
  description = "debug, info, warn or error."
  type        = string
  default     = "info"
}

variable "max_instances" {
  description = "Ceiling on instances per service."
  type        = number
  default     = 4
}

variable "cpu" {
  description = "CPU per instance."
  type        = string
  default     = "1"
}

variable "memory" {
  description = "Memory per instance."
  type        = string
  default     = "512Mi"
}

variable "deletion_protection" {
  description = "Refuse to delete the Cloud Run services."
  type        = bool
  default     = false
}
