package types

import "github.com/cosmos/ibc-go/v8/modules/core/exported"

var (
	_ exported.ClientMessage = (*Misbehaviour)(nil)
	_ exported.ClientMessage = (*FinalizedHeaderMisbehaviour)(nil)
	_ exported.ClientMessage = (*NextSyncCommitteeMisbehaviour)(nil)
)

func (m *Misbehaviour) ClientType() string {
	return ClientType
}

func (m *Misbehaviour) ValidateBasic() error {
	return nil
}

func (m *FinalizedHeaderMisbehaviour) ClientType() string {
	return ClientType
}

func (m *FinalizedHeaderMisbehaviour) ValidateBasic() error {
	return nil
}

func (m *NextSyncCommitteeMisbehaviour) ClientType() string {
	return ClientType
}

func (m *NextSyncCommitteeMisbehaviour) ValidateBasic() error {
	return nil
}
