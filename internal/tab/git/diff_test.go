package git

import "testing"

func TestParseUnifiedDiff(t *testing.T) {
	in := `diff --git a/foo.txt b/foo.txt
index abc..def 100644
--- a/foo.txt
+++ b/foo.txt
@@ -1,3 +1,4 @@
 hello
-world
+World!
+new
diff --git a/bar.bin b/bar.bin
Binary files a/bar.bin and b/bar.bin differ
diff --git a/baz.go b/baz.go
new file mode 100644
index 0000000..1234567
--- /dev/null
+++ b/baz.go
@@ -0,0 +1,2 @@
+package baz
+
`
	files := ParseUnifiedDiff(in)
	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d", len(files))
	}
	if files[0].OldPath != "foo.txt" || files[0].NewPath != "foo.txt" {
		t.Errorf("file 0 paths: %+v", files[0])
	}
	if len(files[0].Hunks) != 1 || len(files[0].Hunks[0].Lines) != 4 {
		t.Errorf("file 0 hunk: %+v", files[0].Hunks)
	}
	if files[0].Hunks[0].OldStart != 1 || files[0].Hunks[0].NewCount != 4 {
		t.Errorf("hunk range: %+v", files[0].Hunks[0])
	}
	if !files[1].IsBinary {
		t.Errorf("expected file 1 binary: %+v", files[1])
	}
	if files[2].OldPath != "baz.go" {
		t.Errorf("file 2 path: %+v", files[2])
	}
}

func TestBuildHunkPatch_Roundtrip(t *testing.T) {
	original := `diff --git a/foo.txt b/foo.txt
index abc..def 100644
--- a/foo.txt
+++ b/foo.txt
@@ -1,3 +1,4 @@
 hello
-world
+World!
+new
`
	files := ParseUnifiedDiff(original)
	if len(files) != 1 || len(files[0].Hunks) != 1 {
		t.Fatalf("parse failed: %+v", files)
	}
	patch := BuildHunkPatch(files[0], files[0].Hunks[0])
	// Re-parse the rebuilt patch to confirm it survives a round-trip.
	roundtrip := ParseUnifiedDiff(patch)
	if len(roundtrip) != 1 || len(roundtrip[0].Hunks) != 1 {
		t.Fatalf("roundtrip parse failed: %s", patch)
	}
	if len(roundtrip[0].Hunks[0].Lines) != 4 {
		t.Errorf("expected 4 lines, got: %+v", roundtrip[0].Hunks[0].Lines)
	}
}
