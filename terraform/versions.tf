terraform {
  required_version = ">= 1.5.0"
  backend "gcs" {
    bucket = "story-6f89f-tfstate"
    prefix = "story-data"
  }
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}
provider "google" {
  project = var.project_id
  region  = var.region
}
