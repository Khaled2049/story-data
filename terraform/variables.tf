variable "project_id" {
  type    = string
  default = "story-6f89f"
}
variable "region" {
  type    = string
  default = "us-central1"
}
variable "image" { type = string }
variable "firebase_project_id" {
  type    = string
  default = "story-6f89f"
}

variable "cors_origins" {
  type = list(string)
  default = [
    "https://story-6f89f.web.app",
    "https://thetaletribe.com",
    "https://www.thetaletribe.com",
    "tauri://localhost",
  ]
}

# Backends that may assert X-User-ID (MCP and the Story Assistant).
variable "service_accounts" {
  type    = list(string)
  default = ["novelsync-agents-run@story-6f89f.iam.gserviceaccount.com"]
}

# Audiences a service ID token may name: this service's own URLs, which can't
# be read from the resource being defined. Cloud Run serves both forms.
variable "service_audiences" {
  type = list(string)
  default = [
    "https://novelsync-story-data-ukvrbnaddq-uc.a.run.app",
    "https://novelsync-story-data-793308156964.us-central1.run.app",
  ]
}

# Bump to rotate the passwords of every database login in database.tf. Each
# login and the secrets holding its URL change together in one apply.
variable "db_password_version" {
  type    = number
  default = 1
}

# Empty creates the alerts with no notification; they still show in the console.
# Set from the ALERT_EMAIL GitHub secret so the address stays out of the repo.
variable "alert_email" {
  type      = string
  default   = ""
  sensitive = true
}

# Pre-launch thresholds; raise them as real assistant and MCP traffic arrives.
variable "alert_service_uids_per_hour" {
  type    = number
  default = 20
}

variable "alert_service_denials_per_10m" {
  type    = number
  default = 10
}
