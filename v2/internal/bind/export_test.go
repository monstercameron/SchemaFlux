package bind

// unregister puts the package back in the state of a program that has not
// registered a root, so that each test of the registration starts from it. It
// exists only for the tests of this package: the registration of the real root
// is for the life of the program.
func unregister() {
	registered.Store(nil)
}
