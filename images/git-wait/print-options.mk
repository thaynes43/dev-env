# Loaded after the pinned Debian rules. This target prints; it never builds/tests.
.PHONY: dev-env-marker-options
dev-env-marker-options:
	@printf '%s\n' $(OPTS)
