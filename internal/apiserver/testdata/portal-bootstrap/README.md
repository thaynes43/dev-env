These disposable envtest fixtures are outside active config/GitOps. They are a
source acceptance example, not an approved production policy or deployment.
The policy and binding match every ConfigMap CREATE in the isolated API server,
then admit only the exact executor, name, namespace and complete CLOSED data.
`failurePolicy: Fail` is exercised by an actual CEL runtime-error test.

The Go fixture creates named target GET and permit GET/UPDATE Roles. CREATE
must be a separate rule without resourceNames: Kubernetes cannot enforce named
CREATE with resourceNames. Extra adversarial fixture Roles deliberately permit
wrong actors/namespaces at the RBAC layer so real admission must deny them.
No such adversarial permission belongs in deployment. No agent role is added.
Production admission activation requires independent exact-head review, source
CI and all negative tests, then separately reviewed GitOps and pinned permit
UID/initial resourceVersion. No activation or runtime acceptance follows here.
