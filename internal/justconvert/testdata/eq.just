set positional-arguments

greet name="world" *rest:
    @echo "hello {{name}}"
    @echo args: {{rest}}
    @printf '[%s]' {{rest}}; echo
    @echo glued={{name}}x
    @echo "pos $1 count $#"

pair a b="bee": (greet a)
    @echo {{a}}-{{b}} "$1/$2"
