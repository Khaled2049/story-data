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
  ]
}
