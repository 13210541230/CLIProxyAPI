package store

// KeyPermissionStore defines permission storage interface
type KeyPermissionStore interface {
	Get(keyID string) (*KeyPermission, error)
	List() ([]*KeyPermission, error)
	Save(perm *KeyPermission) error
	Delete(keyID string) error
}
