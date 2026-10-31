package agentrun

func storageTargetSchema() map[string]any {
	return map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "name", "uid"}, "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"PersistentVolumeClaim", "VolumeSnapshot", "StatefulSet"}}, "name": map[string]any{"type": "string"}, "uid": map[string]any{"type": "string"}}}}
}

func hardwareIntentTool() map[string]any {
	props := map[string]any{}
	for _, key := range []string{"name", "grantUID", "digest", "operation", "host", "device", "firmware", "tomRef"} {
		props[key] = map[string]any{"type": "string"}
	}
	return map[string]any{"name": "hardware_intent", "description": "Default-off RD4 audit intent on the authenticated requester's own installed Active C4 lease. Exact operation/host/device/firmware and a nonsecret tomRef decision reference (for example, Tom's message or the configured delegated-authority reference) are required; agent assertion, not verified human consent. This records no grant decision, changes no keeper approval or credentials, enforces no command or target, and calls no hardware.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": []string{"name", "grantUID", "digest", "operation", "host", "device", "firmware", "tomRef"}}}
}
