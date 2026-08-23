# A whole Launchpad install in one project.
#
# The order matters and Terraform works most of it out on its own: the control
# plane needs the database's secret, the image repository needs to know which
# identities push to it, and the identities do not exist until the control-plane
# module has made them. What Terraform cannot work out is that all of it needs
# the APIs enabled first, which is why that is stated below.
#
# Two things this deliberately does not create. There is no wildcard DNS record
# or load balancer: Cloud Run domain mappings do not accept wildcards, so
# reaching *.your-domain requires a global external load balancer with a
# serverless NEG and a wildcard certificate, and that belongs to whatever
# already terminates TLS for the rest of your estate rather than being invented
# here. And there is no Terraform state backend — the state holds a generated
# database password, so it belongs in a bucket with versioning on and an access
# policy of your choosing, not in a default this module picked for you.

locals {
  services = [
    "run.googleapis.com",
    "cloudbuild.googleapis.com",
    "artifactregistry.googleapis.com",
    "sqladmin.googleapis.com",
    "secretmanager.googleapis.com",
    "iam.googleapis.com",
  ]
}

# Enabled in the example rather than in a module. A module that turns on
# project-wide services as a side effect of being used is a module that is
# unsafe to use twice.
resource "google_project_service" "required" {
  for_each = toset(local.services)

  project = var.project_id
  service = each.value

  # Leaving an API enabled on destroy is the kinder default: another workload in
  # the project may well have come to depend on it.
  disable_on_destroy = false
}

module "database" {
  source = "../../modules/database"

  project_id = var.project_id
  region     = var.region

  # The control plane is the only reader. Nothing else has a reason to hold the
  # platform's own database password.
  secret_accessors = [module.control_plane.control_plane_service_account]

  depends_on = [google_project_service.required]
}

module "images" {
  source = "../../modules/artifact-registry"

  project_id = var.project_id
  region     = var.region

  # The control plane submits the build; Cloud Build is what actually pushes the
  # result, so both need to be able to write.
  writers = [
    module.control_plane.control_plane_service_account,
    module.control_plane.build_service_account,
  ]

  depends_on = [google_project_service.required]
}

module "control_plane" {
  source = "../../modules/control-plane"

  project_id  = var.project_id
  region      = var.region
  image       = var.image
  base_domain = var.base_domain
  log_level   = var.log_level

  artifact_repository      = module.images.repository_id
  database_url_secret_id   = module.database.database_url_secret_id
  database_connection_name = module.database.connection_name

  depends_on = [google_project_service.required]
}
