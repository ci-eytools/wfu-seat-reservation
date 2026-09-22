package storage

import "testing"

func TestSurnameStudentAccounts(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"张20260001", "欧阳20260002", "default"} {
		if _, err := AccountDir(root, name); err != nil {
			t.Fatal(name, err)
		}
	}
	ids, err := Accounts(root)
	if err != nil || len(ids) != 3 {
		t.Fatal(ids, err)
	}
	for _, name := range []string{"../张20260001", "张/20260001", "张\\20260001", "张"} {
		if _, err := AccountDir(root, name); err == nil {
			t.Fatal("unsafe name accepted", name)
		}
	}
}
