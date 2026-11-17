set positional-arguments

alias b := build

# Ship it everywhere
[confirm("Ship it?")]
all target *rest: build (notify target)
    @zdev step "Deploying {{target}}"
    echo "rest: {{rest}}" {{rest}}
    -false
    echo continued \
      line
    cd /tmp

build:
    echo build

[private]
notify who="ops":
    echo "notify {{who}}" glued={{who}}x

summary: build

py:
    #!/usr/bin/env python3
    print("hi")
