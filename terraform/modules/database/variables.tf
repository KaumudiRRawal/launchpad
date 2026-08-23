variable "project_id" {
  description = "Project that owns the instance."
  type        = string
}

variable "region" {
  description = "Region the instance runs in."
  type        = string
}

variable "name" {
  description = "Instance name. Cloud SQL will not reuse a name for a week after the instance is deleted, so a rebuild needs a new one."
  type        = string
  default     = "launchpad"
}

variable "database_version" {
  description = "PostgreSQL version. The schema is exercised in CI against the same major version."
  type        = string
  default     = "POSTGRES_17"
}

variable "tier" {
  description = "Machine type. The control plane's own state is small; the default is the smallest tier that is not shared-core."
  type        = string
  default     = "db-custom-1-3840"
}

variable "disk_size_gb" {
  description = "Initial disk size. Autoresize is on, so this is a floor rather than a limit."
  type        = number
  default     = 20
}

variable "availability_type" {
  description = "ZONAL or REGIONAL. REGIONAL survives losing a zone and costs roughly twice as much."
  type        = string
  default     = "ZONAL"

  validation {
    condition     = contains(["ZONAL", "REGIONAL"], var.availability_type)
    error_message = "availability_type must be ZONAL or REGIONAL."
  }
}

variable "backup_retention_days" {
  description = "How many daily backups to keep."
  type        = number
  default     = 7
}

variable "deletion_protection" {
  description = "Refuse to delete the instance. On by default: this database is the only record of every account, project and deployment the platform has."
  type        = bool
  default     = true
}

variable "secret_accessors" {
  description = "Principals allowed to read the connection URL secret. The control plane's identity belongs here; nothing else does."
  type        = list(string)
  default     = []
}
