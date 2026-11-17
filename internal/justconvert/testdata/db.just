url := "mysql://dev@db/app"

reset:
    #!/bin/sh
    echo "resetting {{url}}" from {{invocation_directory()}}
