package service

type Filter interface {
	Match(*Service) bool
}

type FilterList []Filter

func (fl *FilterList) Match(service *Service) bool {
	for _, filter := range *fl {
		if filter.Match(service) {
			return true
		}
	}

	return false
}
