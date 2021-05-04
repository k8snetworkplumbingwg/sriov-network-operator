package service

type nameFilter struct {
	name string
}

func NewNameFilter(name string) Filter {
	return &nameFilter{name}
}

func (f *nameFilter) Match(service *Service) bool {
	return f.name == service.Name
}
