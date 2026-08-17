resource "google_project_service" "run" {
  service            = "run.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "secretmanager" {
  service            = "secretmanager.googleapis.com"
  disable_on_destroy = false
}

data "google_service_account" "runtime" {
  account_id = "novelsync-story-data-run"
  project    = var.project_id
}

data "google_secret_manager_secret" "database_url" {
  secret_id = "story-data-neon-database-url"
  project   = var.project_id
}

# Shared secret that lets a trusted backend (the MCP server) assert X-User-ID.
# Same value as STORY_DATA_SERVICE_TOKEN in taleTribe-agents. Must be
# pre-created in Secret Manager; see setup.md.
data "google_secret_manager_secret" "service_token" {
  secret_id = "story-data-service-token"
  project   = var.project_id
}

resource "google_secret_manager_secret_iam_member" "database_url" {
  secret_id = data.google_secret_manager_secret.database_url.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${data.google_service_account.runtime.email}"
}

resource "google_secret_manager_secret_iam_member" "service_token" {
  secret_id = data.google_secret_manager_secret.service_token.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${data.google_service_account.runtime.email}"
}

resource "google_cloud_run_v2_service" "api" {
  name     = "novelsync-story-data"
  location = var.region
  ingress  = "INGRESS_TRAFFIC_ALL"

  template {
    service_account = data.google_service_account.runtime.email

    scaling {
      min_instance_count = 0
      max_instance_count = 5
    }

    containers {
      image = var.image

      ports { container_port = 8080 }
      resources { limits = { cpu = "1", memory = "512Mi" } }

      env {
        name  = "AUTH_MODE"
        value = "production"
      }
      env {
        name  = "FIREBASE_PROJECT_ID"
        value = var.firebase_project_id
      }
      env {
        name  = "CORS_ORIGINS"
        value = join(",", var.cors_origins)
      }
      env {
        name = "DATABASE_URL"
        value_source {
          secret_key_ref {
            secret  = data.google_secret_manager_secret.database_url.secret_id
            version = "latest"
          }
        }
      }
      # Empty would disable the service-auth path entirely, which is the
      # fail-safe default in code but would break MCP reads here.
      env {
        name = "SERVICE_TOKEN"
        value_source {
          secret_key_ref {
            secret  = data.google_secret_manager_secret.service_token.secret_id
            version = "latest"
          }
        }
      }
    }
  }

  depends_on = [
    google_project_service.run,
    google_secret_manager_secret_iam_member.database_url,
    google_secret_manager_secret_iam_member.service_token,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "public" {
  name     = google_cloud_run_v2_service.api.name
  location = var.region
  role     = "roles/run.invoker"
  member   = "allUsers"
}

output "service_url" { value = google_cloud_run_v2_service.api.uri }
