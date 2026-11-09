package tools

const defaultSchema = "public"

const schemaAccessibleSQL = `
SELECT EXISTS (
  SELECT 1 FROM pg_namespace
  WHERE nspname = $1 AND has_schema_privilege(oid, 'USAGE')
)`

func schemaOrDefault(s string) string {
	if s == "" {
		return defaultSchema
	}
	return s
}
