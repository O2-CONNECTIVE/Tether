//go:build !windows

package operatorauth

// No child-side launch witness exists outside Windows.
func nativeHelperLaunchWitness() helperLaunchWitness { return nil }
