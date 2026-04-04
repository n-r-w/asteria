#include "greeter.hpp"

const char* Greeter::greet() const {
    return "hello";
}

const char* call_greeter(const Greeter& greeter) {
    return greeter.greet();
}
