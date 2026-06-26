package req

func ApplyHooks(req *Request, hooks []Hook) error {
	for _, hook := range hooks {
		if err := hook(req); err != nil {
			return err
		}
	}
	return nil
}

func Chain(hooks ...Hook) Hook {
	return func(req *Request) error {
		return ApplyHooks(req, hooks)
	}
}
