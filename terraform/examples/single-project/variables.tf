variable "project_id" {
  description = "Project to install Launchpad into. Everything it deploys lands here too."
  type        = string
}

variable "region" {
  description = "Region for the control plane, its database, its images and everything it deploys."
  type        = string
  default     = "europe-west1"
}

variable "image" {
  description = "Control-plane image. Build it with `make image` and push it to the repository this creates."
  type        = string
}

variable "base_domain" {
  description = "Domain environment subdomains are minted under. You must be able to point its wildcard record at the proxy service this outputs."
  type        = string
}

variable "log_level" {
  description = "Control-plane log level."
  type        = string
  default     = "info"
}
