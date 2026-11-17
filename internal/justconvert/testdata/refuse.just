set export
up := uppercase("x")

[no-cd]
a: && b
    echo {{up}}

b:
    #!/usr/bin/env python3
    print("{{up}}")

[linux]
only-linux:
    echo linux
