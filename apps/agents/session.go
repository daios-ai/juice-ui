package agents

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// signIn makes handle's session from its password and records it as
// `juice auth login` would: the kernel in client/config.json, the session in
// client/credentials/<login>.json (ecosystem-standard §4). The password is
// used once and kept nowhere. Nothing is written unless the kernel answered
// a session for exactly that login.
func (a *Agents) signIn(ctx context.Context, handle, password string) error {
	login := a.deps.Address(handle)
	verifier, err := session.NewVerifier()
	if err != nil {
		return err
	}
	code, err := a.deps.Client.Authorize(ctx, login, password, session.Challenge(verifier))
	if err != nil {
		return err
	}
	toks, err := a.deps.Client.ExchangeCode(ctx, code, verifier)
	if err != nil {
		return err
	}
	me, err := a.deps.Client.Me(ctx, toks.AccessToken)
	if err != nil {
		return err
	}
	if me.Address != login {
		return fmt.Errorf("signed in as %s, not %s", me.Address, login)
	}
	if err := a.deps.Client.RecordKernel(ctx, a.client); err != nil {
		return err
	}
	return client.WriteCredential(a.credPath(handle), toks, me.ID)
}

// credPath is the credential file of handle's login.
func (a *Agents) credPath(handle string) string {
	return filepath.Join(a.client, "credentials", a.deps.Address(handle)+".json")
}
