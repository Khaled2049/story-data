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
