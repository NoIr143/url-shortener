variable "domain_name" {
  description = <<-EOT
    Public apex domain for this environment, e.g. "short.example.com".
    Required — no default. No domain has been registered or chosen for
    this project yet (docs/research/T1-04-external-dependency-inventory.md);
    this environment cannot even plan (let alone apply) until a real
    value is supplied at that time, by design.
  EOT
  type        = string
}
