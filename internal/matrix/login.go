package matrix

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const loginFlowID = "messages"

func (c *Connector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "This Mac's Messages",
		Description: "Bridge the Apple Account that Messages on this Mac is signed in to",
		ID:          loginFlowID,
	}}
}

func (c *Connector) CreateLogin(_ context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != loginFlowID {
		return nil, fmt.Errorf("unknown login flow %q", flowID)
	}
	return &login{connector: c, user: user}, nil
}

// login has nothing to ask: Messages is already signed in, so it adopts that account.
type login struct {
	connector *Connector
	user      *bridgev2.User
}

var _ bridgev2.LoginProcess = (*login)(nil)

func (l *login) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	account, err := l.connector.core.Account(ctx)
	if err != nil {
		return nil, err
	}
	userLogin, err := l.user.NewLogin(ctx, &database.UserLogin{
		ID:         networkid.UserLoginID(account.ID),
		RemoteName: account.Handle,
		Metadata:   &UserLoginMetadata{},
	}, &bridgev2.NewLoginParams{LoadUserLogin: l.connector.LoadUserLogin})
	if err != nil {
		return nil, err
	}
	// The client outlives this login request, so it runs on the bridge's lifetime like logins
	// loaded at startup.
	go userLogin.Client.Connect(userLogin.Log.WithContext(l.user.Bridge.BackgroundCtx)) //nolint:contextcheck
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "house.thurstons.imsgbus.complete",
		Instructions: "Bridging " + account.Handle,
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: userLogin.ID,
			UserLogin:   userLogin,
		},
	}, nil
}

func (l *login) Cancel() {}
