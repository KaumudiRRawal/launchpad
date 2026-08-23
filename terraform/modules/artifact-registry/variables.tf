variable "project_id" {
  description = "Project that owns the repository."
  type        = string
}

variable "region" {
  description = "Region the repository lives in. Keep it the same as the region services run in: an image pulled across regions is paid for on every cold start."
  type        = string
}

variable "name" {
  description = "Repository ID. This is the LAUNCHPAD_ARTIFACT_REPOSITORY the control plane is configured with."
  type        = string
  default     = "launchpad"
}

variable "writers" {
  description = "Principals allowed to push images: the control plane's identity and whatever service account Cloud Build runs as."
  type        = list(string)
  default     = []
}

variable "readers" {
  description = "Principals allowed to pull images. Cloud Run in the same project pulls as its own service agent, which already has access, so this is for anything outside it."
  type        = list(string)
  default     = []
}

variable "keep_recent_versions" {
  description = "How many image versions to keep regardless of age. Enough to roll back past a few bad deployments."
  type        = number
  default     = 20

  validation {
    condition     = var.keep_recent_versions >= 1
    error_message = "Keeping zero versions would let the cleanup policy delete the image a live service is running."
  }
}

variable "delete_after_days" {
  description = "Age at which an image beyond keep_recent_versions is deleted."
  type        = number
  default     = 30
}
