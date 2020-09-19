module git.andyleap.dev/gitserve

go 1.14

require (
	github.com/andyleap/go-s3 v0.0.0-20200817073635-84c21cd7ac24
	github.com/andyleap/parser v0.0.0-20160126201130-db5a13a7cd46
	github.com/andyleap/stateparser v0.0.0-00010101000000-000000000000
	github.com/go-git/go-git/v5 v5.1.0
	github.com/go-test/deep v1.0.7
	github.com/jhunt/go-ansi v0.0.0-20181127194324-5fd839f108b6 // indirect
	golang.org/x/net v0.0.0-20200813134508-3edf25e44fcc // indirect
	golang.org/x/sys v0.0.0-20200814200057-3d37ad5750ed // indirect
)

replace github.com/andyleap/stateparser => ../stateparser
