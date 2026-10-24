// SPDX-License-Identifier: MIT
pragma solidity 0.8.19;

// Deliberately controlled counterparts for response and callback failures.
// Normal wrapped-native payments use the retained upstream WZNN bytecode.
contract Token {
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;
    uint256 public calls;
    uint256 public mode;
    uint256 public failAt;
    event Transfer(address indexed from, address indexed to, uint256 amount);
    event Approval(address indexed owner, address indexed spender, uint256 amount);

    function mint(address to, uint256 amount) external { balanceOf[to] += amount; }
    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
        emit Approval(msg.sender, spender, amount);
        return true;
    }
    function configure(uint256 mode_, uint256 failAt_) external {
        mode = mode_; failAt = failAt_; calls = 0;
    }
    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        require(balanceOf[from] >= amount, "fixture balance");
        uint256 approved = allowance[from][msg.sender];
        require(approved >= amount, "fixture allowance");
        if (approved != type(uint256).max) allowance[from][msg.sender] = approved - amount;
        balanceOf[from] -= amount; balanceOf[to] += amount;
        calls++;
        emit Transfer(from, to, amount);
        if (failAt != 0 && calls != failAt) return true;
        uint256 response = mode;
        if (response == 1) assembly { return(0, 0) }
        if (response == 2) return false;
        if (response == 3) revert("fixture rejection");
        if (response == 4) assembly { mstore(0, 1) return(31, 1) }
        if (response == 5) assembly { mstore(0, 2) return(0, 32) }
        if (response == 6) assembly { mstore(0, 1) mstore(32, 99) return(0, 64) }
        return true;
    }
}

contract Receiver {
    uint256 public mode;
    uint256 public attempted;
    address private callbackTarget;
    bytes private callbackData;
    event Received(uint256 amount);
    event Callback(bool ok, bytes result);

    function configure(uint256 mode_, address target, bytes calldata data) external {
        mode = mode_; callbackTarget = target; callbackData = data;
    }
    function execute(address target, bytes calldata data) external payable returns (bytes memory) {
        (bool ok, bytes memory result) = target.call{value: msg.value}(data);
        if (!ok) assembly { revert(add(result, 32), mload(result)) }
        return result;
    }
    receive() external payable {
        if (mode == 1) revert("fixture rejects native");
        if (mode == 2) assembly { for {} 1 {} {} }
        if (mode == 3) { attempted++; revert("fixture rolls back receive"); }
        if (mode == 4) {
            (bool ok, bytes memory result) = callbackTarget.call(callbackData);
            emit Callback(ok, result);
        }
        emit Received(msg.value);
    }
}

contract FaultyWrapper {
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;
    uint256 public failure;
    function configure(uint256 stage) external { failure = stage; }
    function deposit() external payable {
        require(failure != 3, "fixture deposit"); balanceOf[msg.sender] += msg.value;
    }
    function withdraw(uint256 amount) external {
        require(failure != 2, "fixture withdraw");
        balanceOf[msg.sender] -= amount;
        (bool ok,) = msg.sender.call{value: amount}(""); require(ok, "fixture native");
    }
    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount; return true;
    }
    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        require(failure != 1, "fixture transferFrom");
        allowance[from][msg.sender] -= amount;
        balanceOf[from] -= amount; balanceOf[to] += amount; return true;
    }
    function transfer(address to, uint256 amount) external returns (bool) {
        require(failure != 4, "fixture transfer");
        balanceOf[msg.sender] -= amount; balanceOf[to] += amount; return true;
    }
}
