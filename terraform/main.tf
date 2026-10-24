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

# /v1/admin/* verifies tokens with the revocation check, which looks the user up
# in Firebase Auth; without this every admin request fails with 401.
resource "google_project_iam_member" "runtime_firebase_auth_viewer" {
  project = var.project_id
  role    = "roles/firebaseauth.viewer"
  member  = "serviceAccount:${data.google_service_account.runtime.email}"
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

# Full recommendation-signal derivation. This is a job rather than part of the
# API process because it scans the social and reading-progress tables end to
# end. The recommendations stack owns the workflow that invokes it after the
# catalog ingest and before the aggregate refresh.
resource "google_cloud_run_v2_job" "sync_recs" {
  name     = "novelsync-story-data-sync-recs"
  location = var.region

  template {
    task_count  = 1
    parallelism = 1

    template {
      service_account = data.google_service_account.runtime.email
      max_retries     = 1
      timeout         = "600s"

      containers {
        image = var.image
        args  = ["sync-recs"]

        resources {
          limits = {
            cpu    = "1"
            memory = "512Mi"
          }
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
      }
    }
  }

  depends_on = [
    google_project_service.run,
    google_secret_manager_secret_iam_member.database_url,
  ]
}

resource "google_project_service" "cloudscheduler" {
  service            = "cloudscheduler.googleapis.com"
  disable_on_destroy = false
}

# Settles every competition whose voting deadline has passed. SweepDuePhases
# stops at voting on purpose, so without this nothing settles unless a host or
# an admin calls settle by hand.
resource "google_cloud_run_v2_job" "settle_due" {
  name     = "novelsync-story-data-settle-due"
  location = var.region

  template {
    task_count  = 1
    parallelism = 1

    template {
      service_account = data.google_service_account.runtime.email
      # A failure is usually a competition that cannot pay out, which an
      # immediate retry repeats; the next scheduled run retries it anyway.
      max_retries = 0
      timeout     = "600s"

      containers {
        image = var.image
        args  = ["settle-due"]

        resources {
          limits = {
            cpu    = "1"
            memory = "512Mi"
          }
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
      }
    }
  }

  depends_on = [
    google_project_service.run,
    google_secret_manager_secret_iam_member.database_url,
  ]
}

resource "google_service_account" "settle_due_scheduler" {
  project      = var.project_id
  account_id   = "novelsync-settle-due-scheduler"
  display_name = "TheTaleTribe competition settle-due scheduler"
}

resource "google_cloud_run_v2_job_iam_member" "settle_due_scheduler" {
  project  = var.project_id
  name     = google_cloud_run_v2_job.settle_due.name
  location = var.region
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.settle_due_scheduler.email}"
}

resource "google_cloud_scheduler_job" "settle_due" {
  project          = var.project_id
  region           = var.region
  name             = "novelsync-story-data-settle-due"
  description      = "Settle competitions whose voting deadline has passed."
  schedule         = var.settle_due_schedule
  time_zone        = "Etc/UTC"
  attempt_deadline = "60s"

  # Retries the request that starts the job, not a failed execution.
  retry_config {
    retry_count          = 2
    min_backoff_duration = "30s"
    max_backoff_duration = "300s"
  }

  http_target {
    http_method = "POST"
    uri         = "https://run.googleapis.com/v2/projects/${var.project_id}/locations/${var.region}/jobs/${google_cloud_run_v2_job.settle_due.name}:run"

    oauth_token {
      service_account_email = google_service_account.settle_due_scheduler.email
      scope                 = "https://www.googleapis.com/auth/cloud-platform"
    }
  }

  depends_on = [
    google_project_service.cloudscheduler,
    google_cloud_run_v2_job_iam_member.settle_due_scheduler,
  ]
}

output "service_url" { value = google_cloud_run_v2_service.api.uri }

output "sync_recs_job_name" {
  value = google_cloud_run_v2_job.sync_recs.name
}

output "settle_due_job_name" {
  value = google_cloud_run_v2_job.settle_due.name
}
