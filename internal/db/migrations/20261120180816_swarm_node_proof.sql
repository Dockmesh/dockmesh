-- swarm_node_proof
-- trust_root is the Swarm root CA (PEM) node proofs must chain to, pinned
-- from the origin and cross-checked with the other managers. A different
-- root the managers report waits in trust_root_pending until it is
-- accepted.
ALTER TABLE clusters ADD COLUMN trust_root TEXT;
ALTER TABLE clusters ADD COLUMN trust_root_pending TEXT;
ALTER TABLE clusters ADD COLUMN trust_root_pending_at DATETIME;

-- verified_at is set when the host proved it holds the node's key;
-- cert_not_after and cert_issuer describe the certificate it proved with.
ALTER TABLE cluster_memberships ADD COLUMN verified_at DATETIME;
ALTER TABLE cluster_memberships ADD COLUMN cert_not_after DATETIME;
ALTER TABLE cluster_memberships ADD COLUMN cert_issuer TEXT;
