package service

func (s *CallService) SetPublisher(publisher CallEventPublisher) {
	if s != nil {
		s.publisher = publisher
	}
}
