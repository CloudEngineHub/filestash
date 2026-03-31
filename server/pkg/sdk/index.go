package sdk

type Filestash struct {
	Token    string
	URL      string
	Insecure bool
}

func NewClient() Filestash {
	f := Filestash{
		URL: localURL(),
	}
	return f
}
