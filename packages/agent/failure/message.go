// Package failure defines safe, transport-neutral messages for terminal agent
// failures. Raw errors are diagnostic data and must not cross this boundary.
package failure

const ServiceUnavailableMessage = "The service is temporarily unavailable."

// PublicMessage intentionally does not inspect err. Provider and tool errors
// can contain arbitrary upstream bodies, schemas, request data, or secrets;
// only logs and traces may retain those details.
func PublicMessage(error) string {
	return ServiceUnavailableMessage
}
