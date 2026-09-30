// Package body — canonical pairing request construction.
//
// Builds the Doll Network pairing request from the persisted Body identity and
// WG keypair. Carries ONLY the public WG key on the wire; the private key is
// never serialized.
package body

import (
	"github.com/Neon-Dolls/neondoll-shellbody/internal/dollnetwork"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/identity"
)

// BuildPairingBody constructs the canonical pairing body object from a
// persisted identity state.
func BuildPairingBody(st *identity.IdentityState) dollnetwork.PairRequestBody {
	return dollnetwork.PairRequestBody{
		BodyID:         string(st.Identity.BodyID),
		Name:           st.Identity.Name,
		Implementation: st.Identity.Meta.Implementation,
		Platform:       st.Identity.Meta.Platform,
		Arch:           st.Identity.Meta.Architecture,
	}
}

// BuildPairingNetwork builds the network object containing only the WG public
// key (base64). The private key is intentionally absent.
func BuildPairingNetwork(kp *WgKeypair) dollnetwork.PairingNetwork {
	return dollnetwork.PairingNetwork{
		WireGuardPublicKey: kp.PublicKeyBase64(),
	}
}

// BuildPairRequest composes a pairing request from an invitation's id/secret
// plus the Body's identity and WG public key.
func BuildPairRequest(invitationID, secret string, st *identity.IdentityState, kp *WgKeypair) dollnetwork.PairRequest {
	return dollnetwork.PairRequest{
		Version:      dollnetwork.ProtocolVersion,
		InvitationID: invitationID,
		Secret:       secret,
		Body:         BuildPairingBody(st),
		Network:      BuildPairingNetwork(kp),
	}
}
