# Detection for a compromised agents runtime asserting uids it should not
# (security plan phase 4: accepted risk, alerted on rather than prevented).

locals {
  api_log_filter = <<-EOT
    resource.type="cloud_run_revision"
    resource.labels.service_name="${google_cloud_run_v2_service.api.name}"
    jsonPayload.auth="service"
  EOT
}

resource "google_logging_metric" "service_requests" {
  name   = "story_data_service_requests"
  filter = local.api_log_filter
  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
    labels {
      key = "uid"
    }
    labels {
      key = "caller"
    }
  }
  label_extractors = {
    uid    = "EXTRACT(jsonPayload.uid)"
    caller = "EXTRACT(jsonPayload.caller)"
  }
}

# 401, 403 and 404: wrong account, unlisted route, or a story the uid does not own.
resource "google_logging_metric" "service_denials" {
  name   = "story_data_service_denials"
  filter = "${local.api_log_filter}\njsonPayload.status>=400 jsonPayload.status<500"
  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
  }
}

resource "google_monitoring_notification_channel" "email" {
  count        = var.alert_email == "" ? 0 : 1
  display_name = "story-data security alerts"
  type         = "email"
  labels = {
    email_address = var.alert_email
  }
}

resource "google_monitoring_alert_policy" "service_uid_spread" {
  display_name          = "story-data: service calls span many users"
  combiner              = "OR"
  notification_channels = google_monitoring_notification_channel.email[*].id
  conditions {
    display_name = "distinct asserted uids in one hour"
    condition_prometheus_query_language {
      query               = "count(sum by (uid) (increase(logging_googleapis_com:user_${google_logging_metric.service_requests.name}{monitored_resource=\"cloud_run_revision\"}[1h])) > 0) > ${var.alert_service_uids_per_hour}"
      duration            = "0s"
      evaluation_interval = "300s"
    }
  }
  documentation {
    mime_type = "text/markdown"
    content   = "The agents service asserted more distinct uids in an hour than expected. Check `jsonPayload.auth=\"service\"` logs on the story-data API for the uids and paths, and rotate nothing until you know which runtime sent them. See the security plan, phase 4."
  }
}

resource "google_monitoring_alert_policy" "service_denials" {
  display_name          = "story-data: repeated service call denials"
  combiner              = "OR"
  notification_channels = google_monitoring_notification_channel.email[*].id
  conditions {
    display_name = "service calls refused in ten minutes"
    condition_prometheus_query_language {
      query               = "sum(increase(logging_googleapis_com:user_${google_logging_metric.service_denials.name}{monitored_resource=\"cloud_run_revision\"}[10m])) > ${var.alert_service_denials_per_10m}"
      duration            = "0s"
      evaluation_interval = "60s"
    }
  }
  documentation {
    mime_type = "text/markdown"
    content   = "A service caller was refused repeatedly: probing for stories its asserted uid does not own, or routes off the allowlist. Check story-data `request rejected` logs with `jsonPayload.auth=\"service\"`."
  }
}
