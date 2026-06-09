const express = require('express');
const fs = require('fs');
const path = require('path');
const { v4: uuidv4 } = require('uuid');

const app = express();
const PORT = process.env.PORT || 3000;
const DATA_FILE = path.join(__dirname, 'data', 'students.json');

// 中间件
app.use(express.json());
app.use(express.static('public'));

// 确保数据目录存在
if (!fs.existsSync(path.join(__dirname, 'data'))) {
  fs.mkdirSync(path.join(__dirname, 'data'));
}

// 初始化数据文件
if (!fs.existsSync(DATA_FILE)) {
  fs.writeFileSync(DATA_FILE, '[]', 'utf8');
}

// 读取学生数据
function readStudents() {
  try {
    const data = fs.readFileSync(DATA_FILE, 'utf8');
    return JSON.parse(data);
  } catch (error) {
    console.error('读取学生数据失败:', error);
    return [];
  }
}

// 保存学生数据
function saveStudents(students) {
  try {
    fs.writeFileSync(DATA_FILE, JSON.stringify(students, null, 2), 'utf8');
    return true;
  } catch (error) {
    console.error('保存学生数据失败:', error);
    return false;
  }
}

// 验证学生数据
function validateStudent(student) {
  const requiredFields = ['name', 'age', 'gender', 'studentId', 'class'];
  const missingFields = requiredFields.filter(field => !student[field]);
  
  if (missingFields.length > 0) {
    return { valid: false, error: `缺少必填字段: ${missingFields.join(', ')}` };
  }
  
  if (typeof student.age !== 'number' || student.age < 0 || student.age > 150) {
    return { valid: false, error: '年龄必须是0-150之间的数字' };
  }
  
  if (!['男', '女'].includes(student.gender)) {
    return { valid: false, error: '性别必须是"男"或"女"' };
  }
  
  return { valid: true };
}

// API路由

// 获取所有学生
app.get('/api/students', (req, res) => {
  const students = readStudents();
  res.json(students);
});

// 获取单个学生
app.get('/api/students/:id', (req, res) => {
  const students = readStudents();
  const student = students.find(s => s.id === req.params.id);
  
  if (!student) {
    return res.status(404).json({ error: '学生不存在' });
  }
  
  res.json(student);
});

// 添加学生
app.post('/api/students', (req, res) => {
  const validation = validateStudent(req.body);
  if (!validation.valid) {
    return res.status(400).json({ error: validation.error });
  }
  
  const students = readStudents();
  
  // 检查学号是否已存在
  if (students.some(s => s.studentId === req.body.studentId)) {
    return res.status(400).json({ error: '学号已存在' });
  }
  
  const newStudent = {
    id: uuidv4(),
    ...req.body,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString()
  };
  
  students.push(newStudent);
  
  if (saveStudents(students)) {
    res.status(201).json(newStudent);
  } else {
    res.status(500).json({ error: '保存学生数据失败' });
  }
});

// 更新学生
app.put('/api/students/:id', (req, res) => {
  const validation = validateStudent(req.body);
  if (!validation.valid) {
    return res.status(400).json({ error: validation.error });
  }
  
  const students = readStudents();
  const index = students.findIndex(s => s.id === req.params.id);
  
  if (index === -1) {
    return res.status(404).json({ error: '学生不存在' });
  }
  
  // 检查学号是否被其他学生使用
  if (students.some(s => s.studentId === req.body.studentId && s.id !== req.params.id)) {
    return res.status(400).json({ error: '学号已被其他学生使用' });
  }
  
  const updatedStudent = {
    ...students[index],
    ...req.body,
    updatedAt: new Date().toISOString()
  };
  
  students[index] = updatedStudent;
  
  if (saveStudents(students)) {
    res.json(updatedStudent);
  } else {
    res.status(500).json({ error: '保存学生数据失败' });
  }
});

// 删除学生
app.delete('/api/students/:id', (req, res) => {
  const students = readStudents();
  const index = students.findIndex(s => s.id === req.params.id);
  
  if (index === -1) {
    return res.status(404).json({ error: '学生不存在' });
  }
  
  students.splice(index, 1);
  
  if (saveStudents(students)) {
    res.json({ message: '学生删除成功' });
  } else {
    res.status(500).json({ error: '保存学生数据失败' });
  }
});

// 启动服务器
app.listen(PORT, () => {
  console.log(`学生管理系统运行在 http://localhost:${PORT}`);
});