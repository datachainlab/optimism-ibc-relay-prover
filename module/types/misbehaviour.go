package types

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
