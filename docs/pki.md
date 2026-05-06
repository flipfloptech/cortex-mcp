# Cortex MCP Public Key Infrastructure (PKI)

`cortex-mcp` utilizes a traditional Public Key Infrastructure (PKI) to secure communication across the mesh using mutual TLS (mTLS). This ensures that every node in the mesh can cryptographically verify the identity of any other node, providing a zero-trust architecture.

## The Mesh CA (Site CA)

At the root of the trust chain is the **Mesh CA** (also known as the Site CA). 

When the `cortex-mcp` gateway or bootstrap node is first launched, it attempts to load the Mesh CA. If one does not exist, it automatically generates a new Ed25519 Certificate Authority. 

- **Root Execution**: If run as root (e.g. as a systemd service), the Mesh CA is stored at `/opt/cortex-mcp/etc/mesh_ca.json`.
- **User Execution**: If run as a standard user, it is stored at `~/.cortex-mcp/mesh_ca.json`.

*Note: For backwards compatibility, the system will migrate an existing `gateway_ca.json` if one is present.*

## Node Bootstrapping

When you deploy a new fleet node (e.g., using `cortex-mcp install <node_id>`), the deployment process automatically provisions the node with its own identity:
1. The Gateway generates a new certificate and private key for the node, signed by the Mesh CA.
2. The Gateway packages the Node Cert, Node Key, and the Mesh CA public certificate into a bundle.
3. This bundle is securely transferred over the SSH deploy stream before the node initializes its mTLS membrane.
4. The fleet node saves this bundle to its local identity file (usually `/opt/cortex-mcp/etc/identity.json`).

## Managing the PKI via CLI

`cortex-mcp` provides explicit commands to manage and inspect your Mesh CA:

### `cortex-mcp pki show`
Displays the status, location, and cryptographic details of the currently active Mesh CA.

### `cortex-mcp pki generate [--force]`
Forces the generation of a new Mesh CA. If a CA already exists, the command will fail to prevent accidental mesh partition. Use the `--force` flag to explicitly overwrite the existing CA.

### `cortex-mcp pki issue <node_id> [--out <file>]`
Manually issues a node certificate bundle signed by the local Mesh CA. This is useful if you are provisioning nodes out-of-band (e.g. using Ansible, Chef, or Terraform) instead of the built-in SSH deployer. The bundle will be printed to stdout as JSON, or saved to a file if `--out` is specified.

## Troubleshooting Ed25519 Verification Failures

If you encounter errors similar to the following in your logs:
> `x509: certificate signed by unknown authority (possibly because of "x509: Ed25519 verification failure" while trying to verify candidate authority certificate "cortex-mcp-ephemeral-ca")`

This indicates a **split-brain PKI** scenario. This happens when:
1. The local Gateway generated a new Mesh CA (e.g. because its `mesh_ca.json` was deleted).
2. The remote nodes are still using certificates signed by the *previous* Mesh CA.

When the nodes attempt to connect to the Gateway (or to each other), the cryptographic signatures fail validation because the root authorities no longer match.

### Resolution

To resolve this, you must re-provision the fleet nodes with identities signed by the current Mesh CA. 

1. Ensure your Gateway is running with the desired Mesh CA (`cortex-mcp pki show`).
2. Force a re-installation of the fleet nodes and instruct the deployer to wipe their old keys:
   ```bash
   cortex-mcp install <node_id> --force --regenerate-keys
   ```
   *(The `--regenerate-keys` flag ensures the old `identity.json` on the remote node is overwritten).*

Alternatively, if you manage the nodes manually, you can generate a new bundle using `cortex-mcp pki issue <node_id>` and deploy it to `/opt/cortex-mcp/etc/identity.json` on the target nodes.
