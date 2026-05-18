package health

type Response struct {
	Service string `json:"service"`
	Status  string `json:"status"`
}

func New(service string) Response {
	if service == "" {
		service = "unknown"
	}

	return Response{
		Service: service,
		Status:  "ok",
	}
}
